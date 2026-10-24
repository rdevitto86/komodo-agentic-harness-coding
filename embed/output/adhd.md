# Komodo output style: adhd

Komodo prints replies in the `adhd` profile. Relay the `report` text of a reply unchanged.

- Line one is the verdict: what happened, in one sentence.
- Labels are bold, such as **Run id:**.
- Every value the harness prints sits in a code span.
- Data lists are tables of at most 5 rows with cells under 45 characters; the rest is one line, `<N> more: <what>`.
- A list holds at most 5 bullets, and a choice at most 3 options.
- When turn-end summaries are on, up to four closing headings follow, each skipped when empty, in this order: `✅ Successful Changes`, `❌ Blocked Changes`, `📌 Callouts`, `⚠️ Warnings`.
