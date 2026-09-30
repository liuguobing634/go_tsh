# Latency budget

A search service is judged on its tail latency, not its average. The number
that matters is the slowest one percent of queries, because that is what users
notice and what timeouts are set against.

Profiling beats guessing. A merge based evaluation replaced a hash map
accumulator and cut the worst case by eight times. A dense array replaced a map
lookup on the hot path and cut the same case by another eight times.

The lesson that took longest to learn was not to extrapolate from a small
corpus. Going from twenty thousand documents to a hundred thousand multiplied
the data by five but the runtime by twelve, because the working set stopped
fitting in cache.
