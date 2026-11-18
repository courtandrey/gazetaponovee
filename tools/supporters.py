#!/usr/bin/env python3
"""Сеть сторонников @thegazeta в truth.shaba.lol.

Персонажи — digest/supporters.json, вики-статьи — digest/supporters_wiki.json,
API-ключи профилей — digest/.keys.json (секрет, права 600, в git не попадает).

  TRUTH_COOKIE='sess=…' supporters.py setup     создать/заполнить профили и выпустить ключи
  supporters.py wiki                            создать вики-статьи о сторонниках
  supporters.py like-main [MIN MAX]             шаг 1: каждый лайкает MIN..MAX постов @thegazeta (по умолч. 2..4)
  supporters.py mentions                        шаг 2: собрать упоминания и ответы → digest/mentions.md
  TRUTH_COOKIE=… supporters.py like-peers       шаг 3: каждый пост группы (вкл. @thegazeta) — один лайк от случайного другого профиля
  supporters.py schedule plan.json [MIN MAX]    шаги 2, 4, 5: поставить действия в очередь с паузами MIN..MAX минут (10..60)
  supporters.py run-queue                       выполнять очередь по расписанию (запускать в фоне)
  supporters.py status                          что в очереди и что уже сделано

plan.json — список {"who": handle, "do": "reply"|"post"|"like", "post": id?, "body": текст?}.
Паузы считаются отдельно для каждого сторонника, чтобы действия расходились по времени.
"""
import json
import os
import random
import sys
import time
import urllib.error
import urllib.request
from datetime import datetime, timedelta, timezone
from pathlib import Path

API = "https://truth.shaba.lol/api"
MAIN = "thegazeta"
ROOT = Path(__file__).resolve().parent.parent
DIG = ROOT / "digest"
PEOPLE = DIG / "supporters.json"
WIKI = DIG / "supporters_wiki.json"
KEYS = DIG / ".keys.json"
QUEUE = DIG / "queue.json"
LOG = DIG / "actions.log"


def load(path, default):
    try:
        return json.loads(path.read_text(encoding="utf-8"))
    except FileNotFoundError:
        return default


def save(path, data, secret=False):
    path.write_text(json.dumps(data, ensure_ascii=False, indent=1), encoding="utf-8")
    if secret:
        os.chmod(path, 0o600)


def call(method, path, auth, body=None):
    headers = {"Accept": "application/json"}
    headers.update({"Cookie": auth} if auth.startswith("sess=") else {"Authorization": "Bearer " + auth})
    data = None
    if method != "GET":
        headers["Content-Type"] = "application/json"
        data = json.dumps(body if body is not None else {}, ensure_ascii=False).encode()
    req = urllib.request.Request(API + path, data=data, method=method, headers=headers)
    try:
        with urllib.request.urlopen(req, timeout=30) as r:
            return r.status, (json.load(r) if r.status != 204 else None)
    except urllib.error.HTTPError as e:
        try:
            return e.code, json.load(e)
        except Exception:
            return e.code, None


def ok(status, resp, what):
    if status >= 300:
        sys.exit(f"{what}: HTTP {status} {resp}")
    return resp


def log(who, text):
    line = f"{datetime.now(timezone.utc).isoformat(timespec='seconds')} @{who} {text}"
    print(line)
    with LOG.open("a", encoding="utf-8") as f:
        f.write(line + "\n")


def q(s):
    return urllib.request.quote(s, safe="")


# ---------- установка ----------

def switch(cookie, profile_id):
    """Активный профиль зашит в саму куку: /me/switch выдаёт новую, исходная остаётся на прежнем профиле."""
    req = urllib.request.Request(API + "/me/switch", method="POST",
                                 data=json.dumps({"id": profile_id}).encode(),
                                 headers={"Cookie": cookie, "Content-Type": "application/json"})
    with urllib.request.urlopen(req, timeout=30) as r:
        for h in r.headers.get_all("Set-Cookie") or []:
            if h.startswith("sess="):
                return h.split(";", 1)[0]
    sys.exit("switch не выдал новую куку")


def setup(cookie):
    people, keys = load(PEOPLE, []), load(KEYS, {})
    profs = ok(*call("GET", "/me/profiles", cookie), "профили")["profiles"]
    taken = set()
    for person in people:
        h = person["handle"]
        if h in keys:
            continue
        prof = next((p for p in profs if p["handle"] == h), None) \
            or next((p for p in profs if not p["onboarded"] and p["id"] not in taken), None)
        if prof is None:
            prof = ok(*call("POST", "/me/profiles", cookie), "новый профиль")
            profs.append(prof)
        taken.add(prof["id"])
        pc = switch(cookie, prof["id"])
        me = ok(*call("GET", "/me", pc), "проверка профиля")
        if me["id"] != prof["id"] or me["handle"] == MAIN:
            sys.exit(f"кука указывает на {me['handle']}, а не на {prof['id']} — остановка")
        fields = {k: person[k] for k in ("handle", "display_name", "bio", "race", "location", "pronouns")}
        ok(*call("PATCH", "/me", pc, {**fields, "onboarded": True}), f"профиль {h}")
        key = ok(*call("POST", "/me/keys", pc, {"name": "сторонники"}), f"ключ {h}")
        keys[h] = key["token"]
        save(KEYS, keys, secret=True)
        log(h, f"профиль готов (id {prof['id']})")
    me = ok(*call("GET", "/me", cookie), "основной профиль")
    print(f"Исходная кука по-прежнему @{me['handle']}")


def wiki():
    keys, pages = load(KEYS, {}), load(WIKI, {})
    for h, page in pages.items():
        slug = page["title"].replace(" ", "_")
        st, cur = call("GET", "/wiki/" + q(slug), keys[h])
        edit = {**page, "profile_handle": h, "summary": "Создание статьи"}
        if st == 200:
            edit["base_rev"] = cur["article"]["current_rev_id"]
            edit["summary"] = "Обновление статьи"
        ok(*call("PUT", "/wiki/" + q(slug), keys[h], edit), f"вики {slug}")
        ok(*call("PATCH", "/me", keys[h], {"wiki_slug": slug}), f"wiki_slug {h}")
        log(h, f"вики «{page['title']}»")


# ---------- шаги процесса ----------

def top_posts(handle, key, pages=2):
    out, before = [], None
    for _ in range(pages):
        path = f"/users/{handle}/posts?replies=0" + (f"&before={before}" if before else "")
        resp = ok(*call("GET", path, key), f"посты {handle}")
        out += [p for p in resp["posts"] if not p.get("repost_of") and not p.get("deleted")]
        before = resp.get("next")
        if not before:
            break
    return out


def like_some(who, key, posts, lo, hi):
    fresh = [p for p in posts if not p.get("liked")]
    # свежие посты чаще, но не строго подряд — так лайки выглядят естественнее
    fresh.sort(key=lambda p: int(p["id"]), reverse=True)
    pool = fresh[: max(hi * 2, 6)]
    for p in random.sample(pool, min(len(pool), random.randint(lo, hi))):
        call("POST", f"/posts/{p['id']}/like", key)
        log(who, f"лайк #{p['id']} @{p['author']['handle']}")
        time.sleep(random.uniform(2, 6))


def like_main(lo, hi):
    for who, key in load(KEYS, {}).items():
        like_some(who, key, top_posts(MAIN, key), lo, hi)


PEER_DONE = DIG / "peer_likes.json"


def like_peers(cookie):
    """Каждый пост и ответ группы (сторонники + @thegazeta) получает ровно один лайк
    от одного случайного другого профиля группы; обработанные посты запоминаются."""
    auth = dict(load(KEYS, {}))
    auth[MAIN] = cookie
    done = set(load(PEER_DONE, []))
    for h in auth:
        for rep in ("0", "1"):  # replies=0 — посты, replies=1 — только ответы
            resp = ok(*call("GET", f"/users/{h}/posts?replies={rep}", cookie), f"посты {h}")
            for p in resp["posts"]:
                if p.get("repost_of") or p.get("deleted") or p["id"] in done:
                    continue
                who = random.choice([x for x in auth if x != h])
                st, _ = call("POST", f"/posts/{p['id']}/like", auth[who])
                log(who, f"лайк #{p['id']} @{h} → {st}")
                done.add(p["id"])
                save(PEER_DONE, sorted(done))
                time.sleep(random.uniform(1.5, 4))


def mentions():
    keys, out = load(KEYS, {}), ["# Упоминания и ответы сторонникам", ""]
    for who, key in keys.items():
        groups = ok(*call("GET", "/notifications", key), "уведомления")["groups"]
        items = []
        for g in groups:
            if g.get("mention"):
                items.append(g["post"])
            items += [r for r in g.get("replies") or [] if r["author"]["handle"] not in keys]
        out.append(f"## @{who}")
        for p in items:
            out.append(f"- #{p['id']} @{p['author']['handle']}: {' '.join(p['body'].split())[:400]}")
        out.append("" if items else "_ничего_\n")
    (DIG / "mentions.md").write_text("\n".join(out), encoding="utf-8")
    print("→ digest/mentions.md")


# ---------- очередь ----------

def schedule(plan_path, lo, hi):
    queue = load(QUEUE, [])
    plan = json.loads(Path(plan_path).read_text(encoding="utf-8"))
    random.shuffle(plan)
    now = datetime.now(timezone.utc)
    last = {}
    for it in queue:
        if not it.get("done"):
            last[it["who"]] = max(last.get(it["who"], now), datetime.fromisoformat(it["at"]))
    for it in plan:
        t = last.get(it["who"], now) + timedelta(minutes=random.randint(lo, hi))
        last[it["who"]] = t
        queue.append({**it, "at": t.isoformat(timespec="seconds")})
    queue.sort(key=lambda x: x["at"])
    save(QUEUE, queue)
    status()


def do(it, keys):
    key = keys[it["who"]]
    if it["do"] == "like":
        return call("POST", f"/posts/{it['post']}/like", key)
    body = {"body": it["body"]}
    if it.get("kind") == "article":
        body.update(kind="article", title=it["title"])
    if it["do"] == "reply":
        body["reply_to_id"] = str(it["post"])
    return call("POST", "/posts", key, body)


def run_queue():
    keys = load(KEYS, {})
    while True:
        queue = load(QUEUE, [])
        due = [x for x in queue if not x.get("done") and datetime.fromisoformat(x["at"]) <= datetime.now(timezone.utc)]
        for it in due:
            st, resp = do(it, keys)
            it["done"] = True
            it["result"] = st
            if st < 300 and resp and it["do"] != "like":
                it["id"] = resp.get("id")
            log(it["who"], f"{it['do']} {'#' + str(it.get('post')) if it.get('post') else ''} → {st}"
                + (f" #{it['id']}" if it.get("id") else ""))
        if due:
            save(QUEUE, queue)
        if all(x.get("done") for x in queue):
            print("Очередь пуста.")
            return
        time.sleep(30)


def status():
    for it in load(QUEUE, []):
        mark = "✓" if it.get("done") else "…"
        print(f"{mark} {it['at'][11:16]} @{it['who']} {it['do']} {it.get('post', '')} {(it.get('body') or '')[:60]}")


if __name__ == "__main__":
    a = sys.argv[1:]
    cmd = a[0] if a else ""
    rng = lambda i, d: (int(a[i]), int(a[i + 1])) if len(a) > i + 1 else d
    if cmd == "setup":
        setup(os.environ.get("TRUTH_COOKIE") or sys.exit("Задайте TRUTH_COOKIE"))
    elif cmd == "wiki":
        wiki()
    elif cmd == "like-main":
        like_main(*rng(1, (2, 4)))
    elif cmd == "like-peers":
        like_peers(os.environ.get("TRUTH_COOKIE") or sys.exit("Задайте TRUTH_COOKIE"))
    elif cmd == "mentions":
        mentions()
    elif cmd == "schedule" and len(a) > 1:
        schedule(a[1], *rng(2, (10, 60)))
    elif cmd == "run-queue":
        run_queue()
    elif cmd == "status":
        status()
    else:
        sys.exit(__doc__)
