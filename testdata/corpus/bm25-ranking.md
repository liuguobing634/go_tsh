# BM25 ranking

BM25 is the default relevance model in most search engines. It scores a
document for a query term using three ingredients: inverse document frequency,
term frequency saturation, and length normalization.

Inverse document frequency measures how rare a term is. A term that appears in
almost every document carries little information, so it should contribute
little to the score. The smoothed form used by Lucene stays positive even when
a term appears in more than half of the documents, which avoids the odd
behaviour of pushing matching documents below non matching ones.

Term frequency saturation means the tenth occurrence of a word matters much
less than the second. Length normalization penalizes long documents, which
would otherwise win simply by containing more words.
