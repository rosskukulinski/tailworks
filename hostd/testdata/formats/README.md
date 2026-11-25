Golden fixtures (ADR-041 §4). Never edit or regenerate a committed fixture; add a new one for a new version.

Each file is a persisted format exactly as one tailworks version wrote it. The
test next to it decodes every file with the current code, so a change that
can no longer read an old format fails here. To add a fixture for a new
format version, add its generator case and run the test once with
TW_WRITE_FIXTURES=1 (it writes only missing files, never overwrites).
