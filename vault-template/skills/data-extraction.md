---
type: skill
id: data-extraction
name: Data extraction
description: Pull structured records (fields, numbers, dates) out of documents and pages without inventing values.
tools: [web.extract, vault.read]
version: 1
---

- Extract only what the source states. Missing values are `null`, never guesses.
- Copy numbers, currencies, units and dates exactly; normalise formats only when asked
  (dates ISO-8601, money as minor units or with the ISO currency code).
- Keep the source (URL or note path) of every record.
- When a value is ambiguous, keep the raw text in a `note` field.
- If an output schema is given, the answer is exactly one JSON value matching it.
