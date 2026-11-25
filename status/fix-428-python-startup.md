Last update: 2026-11-25 11:30 PT. hostd/gen: Python Workers get 90 s to start in tests (#428).

# fix-428-python-startup

`TestPythonWorker` failed once in CI with "workerd did not come up" after
20 s, and the workerd log was empty. workerd loads Pyodide before it
listens, and stays quiet while it does, so an empty log fits a slow start
rather than a crash. Locally, with a load average of about 44 on 18 CPUs,
the four Python tests took 3.8 s, 5.0 s, 6.2 s and 7.3 s to come up.
JavaScript Workers take a fraction of that. On a CI runner under the whole
`make test`, 20 s is not enough.

This is load-dependent timing. The test checks what a Python Worker does,
not how fast it starts, so the wait is now 90 s for a Python bundle
(`app.Python`) and stays 20 s for JavaScript. Each start's time is logged
(`workerd up in …`), so the margin shows in `-v` output.

Not changed: production hostd also waits 20 s for a new instance to be
healthy (`waitHealthy(20 * time.Second)`, hostd/workload.go). A Python
deploy on a heavily loaded host could hit that too. That would be a
product change, so it's left for a separate issue.
