# Boolean queries

A boolean query combines clauses with AND, OR and NOT. The must clauses have to
match, the should clauses only need one of them to match, and the must not
clauses must not match at all.

When a must clause is present, should clauses stop acting as a filter and only
contribute extra score. This is what lets a query express "this term is
required, and that term is a bonus".

A query made only of negated clauses has no positive clause to start from, so
the engine has to walk every document and subtract the exclusions. That works
but the cost grows with the size of the index.
