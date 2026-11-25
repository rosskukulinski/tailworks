# doctor-readable

`tw doctor` human output: a summary by default (counts, problems with fixes,
one line per group of passing checks; skips counted only), `-v` for the old
full-detail layout. JSON gains `checks[].group`. The renderer is in
`cmd/tw/doctor_render.go`; checks are unchanged except `binaries`, which is
`info` (not `warn`) in single-process mode.
