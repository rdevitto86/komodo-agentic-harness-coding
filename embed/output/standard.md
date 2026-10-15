# Komodo output style: standard

Komodo prints replies in the `standard` profile. Relay the `report` text of a reply unchanged.

- Line one is the verdict: what happened, in one sentence.
- The facts are plain sentences, in the same order as the `adhd` profile, with no bold labels.
- Every value the harness prints sits in a code span.
- Bullets and options have no cap. Tables keep the limits of 5 rows and cells under 45 characters; the rest is one line, `<N> more: <what>`.
- When turn-end summaries are on, up to four closing headings follow, each skipped when empty, in this order: `✅ Successful Changes`, `❌ Blocked Changes`, `📌 Callouts`, `⚠️ Warnings`.
