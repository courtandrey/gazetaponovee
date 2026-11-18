#!/usr/bin/env python3
"""Сбор новых постов из truth.shaba.lol для подготовки новостей.

  TRUTH_COOKIE='sess=…' python3 tools/truth_digest.py collect [--all]
      Скачивает посты новее последнего зафиксированного и пишет их в
      digest/posts-<id>.md (читаемо) и .json (сырые данные).
      --all игнорирует сохранённое состояние (базовый прогон).

  python3 tools/truth_digest.py commit
      Фиксирует последний собранный пост: следующий collect начнёт после него.
      Запускать, когда новости по сбору разобраны.

  python3 tools/truth_digest.py submit news.json [--site https://…]
      Отправляет одобренные новости в форму /submit газеты. news.json — список
      объектов {title, body, author?, rubric?}.

Куки берётся только из переменной окружения и никуда не записывается.
"""
import json
import os
import sys
import time
import urllib.parse
import urllib.request
import uuid
from pathlib import Path

API = "https://truth.shaba.lol/api"
ROOT = Path(__file__).resolve().parent.parent
OUT = ROOT / "digest"
STATE = OUT / "state.json"
PENDING = OUT / ".pending.json"
SITE = "https://gazetaponovee.democrat"


def load(path, default):
    try:
        return json.loads(path.read_text(encoding="utf-8"))
    except FileNotFoundError:
        return default


def api(path, cookie):
    req = urllib.request.Request(API + path, headers={"Cookie": cookie, "Accept": "application/json"})
    with urllib.request.urlopen(req, timeout=30) as resp:
        return json.load(resp)


def fetch_since(last_id, cookie):
    """Посты с id > last_id, от старых к новым. Лента sort=all отдаёт всё, включая ответы."""
    posts, before = [], None
    while True:
        q = {"limit": 30, "sort": "all"}
        if before:
            q["before"] = before
        page = api("/feed?" + urllib.parse.urlencode(q), cookie)
        batch = page.get("posts", [])
        fresh = [p for p in batch if int(p["id"]) > last_id]
        posts += fresh
        before = page.get("next")
        if not batch or not before or len(fresh) < len(batch):
            break
        time.sleep(0.3)
    uniq = {p["id"]: p for p in posts}
    return sorted(uniq.values(), key=lambda p: int(p["id"]))


def who(a):
    bits = [a.get("display_name") or a.get("handle"), "@" + a.get("handle", "")]
    meta = ", ".join(x for x in (a.get("race"), a.get("location")) if x)
    return " ".join(bits) + (f" ({meta})" if meta else "")


def short(text, n):
    text = " ".join((text or "").split())
    return text if len(text) <= n else text[: n - 1] + "…"


def render(posts, cookie):
    authors, lines = {}, []
    for p in posts:
        a = p["author"]
        authors[a["handle"]] = a
        if p.get("deleted"):
            continue
        head = f"### #{p['id']} {who(a)}"
        stats = f"♥{p.get('like_count', 0)} ↻{p.get('repost_count', 0)} 💬{p.get('reply_count', 0)}"
        lines.append(f"{head}  [{p['created_at'][:16].replace('T', ' ')}] {stats}")
        if p.get("repost_of"):
            r = p["repost_of"]
            lines.append(f"> репост #{r.get('id')} от {who(r.get('author', {}))}: {short(r.get('body'), 400)}")
        if p.get("reply_to"):
            r = p["reply_to"]
            rid = r.get("id") if isinstance(r, dict) else r
            lines.append(f"(ответ на #{rid})")
        if p.get("title"):
            lines.append(f"**{p['title']}**")
        if p.get("body"):
            lines.append(p["body"].strip())
        if p.get("quote"):
            q = p["quote"]
            lines.append(f"> цитирует #{q.get('id')} {who(q.get('author', {}))}: {short(q.get('body'), 400)}")
        if p.get("link"):
            lines.append(f"ссылка: {json.dumps(p['link'], ensure_ascii=False)[:300]}")
        for m in p.get("media") or []:
            lines.append(f"медиа: {m.get('url') if isinstance(m, dict) else m}")
        lines.append("")
    people = ["## Авторы", ""]
    for a in sorted(authors.values(), key=lambda a: a.get("display_name", "")):
        people.append(f"- {who(a)}: {short(a.get('bio'), 300)}")
    return "\n".join(people) + "\n\n## Посты\n\n" + "\n".join(lines)


def collect(cookie, full):
    state = load(STATE, {"last_id": 0})
    last = 0 if full else int(state.get("last_id", 0))
    posts = fetch_since(last, cookie)
    if not posts:
        print(f"Новых постов после #{last} нет.")
        return
    OUT.mkdir(exist_ok=True)
    top = posts[-1]["id"]
    stem = OUT / f"posts-{posts[0]['id']}-{top}"
    stem.with_suffix(".json").write_text(json.dumps(posts, ensure_ascii=False, indent=1), encoding="utf-8")
    stem.with_suffix(".md").write_text(render(posts, cookie), encoding="utf-8")
    PENDING.write_text(json.dumps({"last_id": int(top)}), encoding="utf-8")
    print(f"Собрано {len(posts)} постов (#{posts[0]['id']}…#{top}) → {stem}.md")
    print("После разбора выполните `commit`, чтобы следующий сбор начался с #%s." % top)


def commit():
    pending = load(PENDING, None)
    if not pending:
        sys.exit("Нечего фиксировать: сначала collect.")
    STATE.write_text(json.dumps(pending), encoding="utf-8")
    PENDING.unlink()
    print(f"Следующий сбор начнётся после #{pending['last_id']}.")


def submit(path, site):
    items = json.loads(Path(path).read_text(encoding="utf-8"))
    for it in items:
        boundary = uuid.uuid4().hex
        body = b""
        for k in ("title", "body", "author", "rubric"):
            if it.get(k):
                body += (f"--{boundary}\r\nContent-Disposition: form-data; name=\"{k}\"\r\n\r\n{it[k]}\r\n").encode()
        body += f"--{boundary}--\r\n".encode()
        req = urllib.request.Request(site.rstrip("/") + "/submit", data=body, method="POST", headers={
            "Content-Type": f"multipart/form-data; boundary={boundary}",
            "Origin": site.rstrip("/"),
        })
        with urllib.request.urlopen(req, timeout=30) as resp:
            ok = resp.status == 200 and "Материал у редакции" in resp.read().decode("utf-8", "replace")
        print(("✓ " if ok else "✗ ") + it["title"])
        time.sleep(0.5)


if __name__ == "__main__":
    args = sys.argv[1:]
    cmd = args[0] if args else ""
    if cmd == "collect":
        cookie = os.environ.get("TRUTH_COOKIE") or sys.exit("Задайте TRUTH_COOKIE='sess=…'")
        collect(cookie, "--all" in args)
    elif cmd == "commit":
        commit()
    elif cmd == "submit" and len(args) >= 2:
        site = args[args.index("--site") + 1] if "--site" in args else SITE
        submit(args[1], site)
    else:
        sys.exit(__doc__)
