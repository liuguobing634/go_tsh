# Phrase queries

A phrase query requires the terms to appear next to each other, in order,
inside a single field. Positions are stored alongside each posting for exactly
this purpose.

The matching rule is that the distance between two terms in the document has to
equal the distance between them in the query, not simply one. If a stop word
was removed from the middle of the query, the remaining terms are two apart,
and a document where they are adjacent must not match.

Positions restart at zero for every field, so a phrase can never span two
fields. Joining positions across fields would invent phrases that were never
written.
