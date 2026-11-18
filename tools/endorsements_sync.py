#!/usr/bin/env python3
"""Выгрузка темы «Эндорсменты & Донаты» через tdl и отправка на сайт (/endorsements).

  python3 tools/endorsements_sync.py           выгрузить и отправить
  python3 tools/endorsements_sync.py --dry     только показать, что будет отправлено

Запускается на сервере по cron раз в 20 минут:

  */20 * * * * python3 /путь/к/gazetaponovee/tools/endorsements_sync.py >> ~/endorsements_sync.log 2>&1

Каждый раз уходит вся тема целиком, и сайт заменяет свой снимок: удалённые в теме
сообщения (например, тестовые) пропадают и со страницы. Берутся только сообщения
GM-бота; разбор текста делает сайт. Если tdl упал, на сайт ничего не отправляется.
"""
import json
import os
import subprocess
import sys
import tempfile
import urllib.request
from datetime import datetime
from pathlib import Path

TDL = os.environ.get("TDL", str(Path.home() / ".local/bin/tdl"))
CHAT = os.environ.get("ENDORSE_CHAT", "4410136469")    # форум «Девять Штатов»
TOPIC = os.environ.get("ENDORSE_TOPIC", "3744")        # «Эндорсменты & Донаты»
BOT = int(os.environ.get("ENDORSE_BOT", "9259290467"))  # @statesgm_bot
SITE = os.environ.get("GAZETA_INTERNAL", "http://127.0.0.1:" + os.environ.get("INTERNAL_PORT", "9090"))


def export():
    with tempfile.TemporaryDirectory() as tmp:
        out = Path(tmp) / "out.json"
        cmd = [TDL, "chat", "export", "-c", CHAT, "--topic", TOPIC, "--all", "--with-content", "--raw",
               "-o", str(out), "--disable-progress-ps"]
        res = subprocess.run(cmd, capture_output=True, text=True, timeout=600)
        if res.returncode != 0 or not out.exists():
            sys.exit(f"tdl: {res.stderr.strip()[-500:]}")
        return json.loads(out.read_text(encoding="utf-8"))["messages"]


def from_bot(m):
    raw = m.get("raw") or {}
    return (raw.get("FromID") or {}).get("UserID") == BOT and not (raw.get("FwdFrom") or {}).get("FromID")


def main():
    msgs = [{"id": m["id"], "date": m["date"], "text": m.get("text") or ""} for m in export() if from_bot(m)]
    msgs.sort(key=lambda m: m["id"])
    stamp = datetime.now().strftime("%Y-%m-%d %H:%M")
    if "--dry" in sys.argv:
        for m in msgs:
            print(f"#{m['id']}: {m['text']!r}")
        print(f"{stamp} сообщений бота: {len(msgs)}")
        return
    req = urllib.request.Request(SITE + "/internal/endorsements", data=json.dumps({"messages": msgs}).encode(),
                                 headers={"Content-Type": "application/json"}, method="POST")
    with urllib.request.urlopen(req, timeout=60) as resp:
        print(f"{stamp} {resp.read().decode().strip()}")


if __name__ == "__main__":
    main()
