"""Regenerates testdata/seed/*/*.eml. Usage: python3 -I tools/gen_seed.py testdata/seed

The fixtures are committed; run this only when changing the seed. New messages
should be appended to a folder's section so existing file numbers stay stable."""
import os, sys, datetime, re
from email.header import Header
from email.utils import format_datetime, formataddr

out = sys.argv[1]
ME = "me@quietbox.test"
def d(y, m, dd, h=9):
    return datetime.datetime(y, m, dd, h, 0, 0, tzinfo=datetime.timezone.utc)

LIST = lambda ident, name: {
    "List-Id": f"{name} <{ident}>",
    "List-Unsubscribe": f"<https://{ident}/unsub>, <mailto:unsub@{ident}>",
    "List-Unsubscribe-Post": "List-Unsubscribe=One-Click",
}
msgs = []  # (folder, slug, from, to, subject, date, headers, body)
def add(folder, slug, frm, to, subj, date, headers=None, body="Hello.\n", mid="auto"):
    msgs.append(dict(folder=folder, slug=slug, frm=frm, to=to, subj=subj, date=date,
                     headers=headers or {}, body=body, mid=mid))

# Sent: addresses I have written to
add("Sent", "to-alice", ME, "alice@friends.test", "Dinner Friday?", d(2026,1,10), body="Are you free Friday?\n")
add("Sent", "to-bob", ME, "bob@work.test", "Project kickoff", d(2026,2,3), mid="<sent-bob-1@quietbox.test>", body="Kickoff at 10.\n")
add("Sent", "to-eve", ME, "eve@club.test", "Membership question", d(2026,2,20), body="Quick question about membership.\n")
add("Sent", "to-dave", ME, "dave@old.test", "Hello again", d(2026,3,2), body="Long time!\n")

# INBOX
for i, day in enumerate((3, 12, 25), 1):
    add("INBOX", f"alice-{i}", "alice@friends.test", ME, f"Re: Dinner Friday? ({i})", d(2026,5,day), body="Sounds good.\n")
add("INBOX", "bob-reply", "bob@work.test", ME, "Re: Project kickoff", d(2026,5,14),
    headers={"In-Reply-To": "<sent-bob-1@quietbox.test>", "References": "<sent-bob-1@quietbox.test>"}, body="Thanks, see you there.\n")
add("INBOX", "carol-nomid", "carol@unknown.test", ME, "Hi, a question", d(2026,5,20), mid=None, body="Can we talk?\n")
zoe = formataddr((str(Header("Zoë Müller", "utf-8")), "zoe@intl.test"))
add("INBOX", "zoe-encoded", zoe, ME, str(Header("Réunion à 15h — café ☕", "utf-8")), d(2026,5,22), body="A bientôt.\n")
for i, (m, dd) in enumerate(((9, 4), (10, 2)), 1):
    add("INBOX", f"oldnews-{i}", "oldnews@letters.test", ME, f"Letters weekly #{i}", d(2025,m,dd),
        headers=LIST("letters.test", "Letters Weekly"), body="Old issue.\n")
add("INBOX", "newsub-1", "fresh@substack.test", ME, "A brand new essay", d(2026,5,28),
    headers=dict(LIST("fresh.substack.test", "Fresh Essays"), **{"X-Mailer": "Substack"}), body="New essay.\n")
DUP = "<dup-1@promos.travel.test>"
add("INBOX", "dup-promo", "promos@travel.test", ME, "Summer fares", d(2026,5,18), mid=DUP, body="Fares from 49.\n")

# SaneNews
for w in (1, 2):
    for i in (1, 2):
        add("SaneNews", f"writer{w}-{i}", f"writer{w}@substack.test", ME, f"Essay {w}.{i}", d(2026,3+i,5+w),
            headers=dict(LIST(f"writer{w}.substack.test", f"Writer {w}"), **{"X-Mailer": "Substack"}))
for i in (1, 2):
    add("SaneNews", f"buttondown-{i}", "digest@buttondown.test", ME, f"Digest #{i}", d(2026,4,i*7),
        headers=LIST("digest.buttondown.test", "Digest"))
for i in (1, 2):
    add("SaneNews", f"mailchimp-{i}", "campaign@mcsv.test", ME, f"Campaign {i}", d(2026,4,10+i),
        headers={"X-Mailer": "Mailchimp Mailer - **CID1234**", "List-Unsubscribe": "<https://mcsv.test/unsub>"})
for i in range(1, 5):
    add("SaneNews", f"eve-{i}", "eve@club.test", ME, f"Club newsletter {i}", d(2026,2+i//2,3+i*3),
        headers=LIST("club.test", "Club News"))
for i in (1, 2):
    add("SaneNews", f"drift-recent-{i}", "drift@mixed.test", ME, f"Drift now news {i}", d(2026,5,5*i),
        headers=LIST("drift.mixed.test", "Drift"))

# More substack senders, appended last so earlier file numbers do not change.
# With writer1-4 in SaneNews and fresh@ in INBOX, substack.test has 4 of 5
# senders on one route: exactly the 0.8 domain-default threshold.
for w in (3, 4):
    add("SaneNews", f"writer{w}-1", f"writer{w}@substack.test", ME, f"Essay {w}.1", d(2026,4,6+w),
        headers=dict(LIST(f"writer{w}.substack.test", f"Writer {w}"), **{"X-Mailer": "Substack"}))

# SaneBulk
for i, s in enumerate(("Your receipt #1001", "Your receipt #1002"), 1):
    add("SaneBulk", f"shop-{i}", "noreply@shop.test", ME, s, d(2026,4,i*9), headers={"Precedence": "bulk", "X-Mailer": "SendGrid"})
for i in (1, 2):
    add("SaneBulk", f"forge-{i}", "notifications@forge.test", ME, f"[forge] Build {i} passed", d(2026,5,i*6),
        headers={"Auto-Submitted": "auto-generated"})
add("SaneBulk", "negozio-1", "ricevute@negozio.test", ME, "Ricevuta del tuo ordine n. 8841", d(2026,5,3), body="Grazie per il tuo acquisto.\n")
add("SaneBulk", "negozio-2", "ricevute@negozio.test", ME, "Il tuo ordine è stato spedito", d(2026,5,6), body="Il pacco è in viaggio.\n")
add("SaneBulk", "bigstore-receipt", "store@bigstore.test", ME, "Your order receipt", d(2026,4,2))
add("SaneBulk", "bigstore-ship-1", "store@bigstore.test", ME, "Your order has shipped", d(2026,4,4))
add("SaneBulk", "bigstore-ship-2", "store@bigstore.test", ME, "Delivery update: out for delivery", d(2026,4,6))

# SaneLater
for i in (1, 2):
    add("SaneLater", f"bigstore-deal-{i}", "store@bigstore.test", ME, f"Weekend deals {i}", d(2026,4,10+i*3))
for i in (1, 2):
    add("SaneLater", f"travel-{i}", "promos@travel.test", ME, f"Fares you may like {i}", d(2026,3,i*8))
add("SaneLater", "travel-dup", "promos@travel.test", ME, "Summer fares", d(2026,5,18), mid=DUP, body="Fares from 49.\n")
for i, (m, dd) in enumerate(((10, 5), (11, 9), (12, 14)), 1):
    add("SaneLater", f"drift-old-{i}", "drift@mixed.test", ME, f"Drift older {i}", d(2025,m,dd))

# SaneBlackHole
for i in (1, 2):
    add("SaneBlackHole", f"pushy-{i}", "spam@pushy.test", ME, f"AMAZING OFFER {i}!!!", d(2026,5,10+i))

counts = {}
for n, m in enumerate(msgs):
    counts[m["folder"]] = counts.get(m["folder"], 0) + 1
    seq = counts[m["folder"]]
    mid = m["mid"]
    if mid == "auto":
        mid = f"<{m['folder'].lower()}-{m['slug']}@seed.quietbox.test>"
    lines = [f"From: {m['frm']}", f"To: {m['to']}", f"Subject: {m['subj']}", f"Date: {format_datetime(m['date'])}"]
    if mid: lines.append(f"Message-ID: {mid}")
    for k, v in m["headers"].items(): lines.append(f"{k}: {v}")
    lines += ["MIME-Version: 1.0", "Content-Type: text/plain; charset=utf-8", "Content-Transfer-Encoding: 8bit", ""]
    text = "\r\n".join(lines) + "\r\n" + m["body"].replace("\n", "\r\n")
    path = os.path.join(out, m["folder"]); os.makedirs(path, exist_ok=True)
    with open(os.path.join(path, f"{seq:03d}-{m['slug']}.eml"), "wb") as f:
        f.write(text.encode("utf-8"))
print(counts, sum(counts.values()))
