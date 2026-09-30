# Inverted index

An inverted index maps each term to the list of documents that contain it.
That list is called a posting list, and it is the core data structure behind
every full text search engine.

The index is called "inverted" because it reverses the natural direction of the
data: instead of asking "what terms does this document contain", we ask
"which documents contain this term".

Because posting lists are kept sorted by document id, an intersection of two
terms can be computed with a single merge pass instead of a nested loop.
