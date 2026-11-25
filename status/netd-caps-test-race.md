# netd-caps-test-race

Fixes #271: `TestServiceCapsScopedToVIP` wrote `OmitAuth` on netd's shared
LocalClient while `watchRejected` read it (race). The write is removed; netd
already sets it. `go test -race -count=20 -run TestServiceCapsScopedToVIP
./netd`, `go test -race ./netd/...` and `make lint` pass.
