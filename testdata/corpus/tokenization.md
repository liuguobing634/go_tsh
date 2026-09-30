# Tokenization

Tokenization splits raw text into the terms that will actually be indexed.
The same analyzer must be used when writing documents and when parsing queries,
otherwise a document that clearly exists will never be found.

Lowercasing folds "Go" and "GO" onto the same term. Stop word removal drops
words such as "the" and "and", but the positions they occupied are still
counted, so that phrase matching keeps working across a removed word.

Chinese and Japanese do not separate words with spaces, so without a dictionary
the fallback is to index each character on its own. That is crude, but it makes
single character lookups work at all.
