#!/usr/bin/env python3
"""Сбор сообщений из Telegram-чатов игры «Девять Штатов» (через tdl).

  chat_digest.py collect [--all]   выгрузить новые сообщения → digest/chat/chat-<дата>.md
  chat_digest.py commit            зафиксировать: следующий collect начнётся после собранного

Чаты — список CHATS ниже. В форуме темы с пометкой «(не ролплей)» пропускаются.
Нужен залогиненный tdl (`tdl login -T code`). Телефоны участников не сохраняются:
в карту имён попадают только имя и @username.
"""
import json
import subprocess
import sys
import tempfile
from datetime import datetime, timedelta, timezone
from pathlib import Path

# id, заголовок в выгрузке, форум ли (выгрузка по темам)
CHATS = [
    ("4410136469", "Девять Штатов", True),
    ("5443187048", "Золотые (партийный чат)", False),
    ("9259290467", "GM-bot (личный чат)", False),
]
TDL = str(Path.home() / ".local/bin/tdl")
ROOT = Path(__file__).resolve().parent.parent
OUT = ROOT / "digest" / "chat"
STATE = OUT / "state.json"
PENDING = OUT / ".pending.json"
SKIP = "(не ролплей)"
MSK = timezone(timedelta(hours=3))
WHOLE = "_"  # ключ состояния для чата без тем
KNOWN = {}  # имена из всех чатов: в закрытых группах берём известные по другим чатам


def load(path, default):
    try:
        return json.loads(path.read_text(encoding="utf-8"))
    except FileNotFoundError:
        return default


def load_state(full):
    state = {} if full else load(STATE, {})
    # старый формат: {тема: id} только для форума «Девять Штатов»
    if state and all(isinstance(v, int) for v in state.values()):
        state = {CHATS[0][0]: state}
    return state


def tdl(*args, soft=False):
    with tempfile.TemporaryDirectory() as tmp:
        out = Path(tmp) / "out.json"
        cmd = [TDL, *args, "-o", str(out), "--disable-progress-ps"]
        res = subprocess.run(cmd, capture_output=True, text=True)
        if res.returncode != 0 or not out.exists():
            if soft:
                return None
            sys.exit(f"tdl {' '.join(args[:2])}: {res.stderr.strip()[-500:]}")
        return json.loads(out.read_text(encoding="utf-8"))


def chat_info(chat):
    res = subprocess.run([TDL, "chat", "ls", "-o", "json", "-f", f"ID == {chat}"], capture_output=True, text=True)
    return json.loads(res.stdout[res.stdout.index("["):])[0]


def names(chat, info):
    if info.get("type") == "private":
        return {int(chat): info.get("visible_name") or "собеседник"}
    d = tdl("chat", "users", "-c", chat, soft=True) or {}  # не во всех группах список участников открыт
    people = {}
    for group in ("admins", "bots", "users"):
        for u in d.get(group) or []:
            full = " ".join(x for x in (u.get("first_name"), u.get("last_name")) if x) or "?"
            people[u["id"]] = full + (f" @{u['username']}" if u.get("username") else "")
    return people


def export(chat, topic_id, after):
    args = ["chat", "export", "-c", chat, "--all", "--with-content", "--raw"]
    if topic_id:
        args += ["--topic", str(topic_id)]
    if after:
        args += ["-T", "id", "-i", f"{after + 1},2147483647"]
    msgs = tdl(*args)["messages"]
    return sorted((m for m in msgs if m["id"] > after and m["id"] != topic_id), key=lambda m: m["id"])


def line(m, people, private_peer):
    raw = m.get("raw") or {}
    sender = (raw.get("FromID") or {}).get("UserID")
    if private_peer:
        who = "я" if raw.get("Out") else people.get(private_peer, "собеседник")
    else:
        who = people.get(sender, f"id{sender}" if sender else "канал")
    when = datetime.fromtimestamp(m["date"], MSK).strftime("%d.%m %H:%M")
    reply = raw.get("ReplyTo") or {}
    forum_root = reply.get("ForumTopic") and not reply.get("ReplyToTopID")
    to = None if forum_root else reply.get("ReplyToMsgID")
    text = (m.get("text") or "").strip()
    if raw.get("Media"):
        text = ("[медиа] " + text).strip()
    fwd = raw.get("FwdFrom") or {}
    if fwd.get("FromName") or fwd.get("FromID"):
        text = "[пересылка] " + text
    head = f"**#{m['id']}** {when} {who}" + (f" (ответ на #{to})" if to else "")
    return f"{head}: {text}"


def sections(chat, title, forum, state):
    """→ (части выгрузки, новое состояние чата, число сообщений)"""
    info = chat_info(chat)
    KNOWN.update(names(chat, info))
    people = KNOWN
    private_peer = int(chat) if info.get("type") == "private" else None
    targets = [(t["id"], t["title"]) for t in info.get("topics") or [] if SKIP not in t["title"]] if forum \
        else [(None, None)]
    parts, new_state, total = [], {}, 0
    for tid, ttitle in targets:
        key = str(tid) if tid else WHOLE
        after = int(state.get(key, 0))
        msgs = export(chat, tid, after)
        new_state[key] = max([after] + [m["id"] for m in msgs])
        if not msgs:
            continue
        total += len(msgs)
        parts.append(f"## {title}{' / ' + ttitle if ttitle else ''} ({len(msgs)})\n")
        parts += [line(m, people, private_peer) for m in msgs]
        parts.append("")
    return parts, new_state, total


def collect(full):
    state = load_state(full)
    pending, parts, total = {}, [], 0
    for chat, title, forum in CHATS:
        p, s, n = sections(chat, title, forum, state.get(chat, {}))
        parts += p
        pending[chat] = s
        total += n
        print(f"{title}: {n}")
    if not total:
        print("Новых сообщений нет.")
        return
    stamp = datetime.now(MSK).strftime("%Y-%m-%d-%H%M")
    path = OUT / f"chat-{stamp}.md"
    path.write_text(f"# Чаты игры «Девять Штатов», выгрузка {stamp}\n\n" + "\n".join(parts), encoding="utf-8")
    PENDING.write_text(json.dumps(pending), encoding="utf-8")
    print(f"Собрано {total} сообщений → {path}")
    print("После разбора выполните `commit`.")


def commit():
    pending = load(PENDING, None)
    if pending is None:
        sys.exit("Нечего фиксировать: сначала collect.")
    STATE.write_text(json.dumps(pending), encoding="utf-8")
    PENDING.unlink()
    print("Зафиксировано: " + "; ".join(f"{c}: {len(s)} лент" for c, s in pending.items()))


if __name__ == "__main__":
    a = sys.argv[1:]
    if a[:1] == ["collect"]:
        OUT.mkdir(parents=True, exist_ok=True)
        collect("--all" in a)
    elif a[:1] == ["commit"]:
        commit()
    else:
        sys.exit(__doc__)
