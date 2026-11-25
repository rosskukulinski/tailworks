package version

// WorkerdPin is the workerd npm version this tailworks is built and tested
// against. It must equal package-lock.json's node_modules/workerd version
// (TestWorkerdPinMatchesLockfile fails otherwise): bump workerd with
// `npm install workerd@<v>` and update this line in the same PR
// (ADR-041 §6).
const WorkerdPin = "1.20261121.1"

// WorkerdPinDate is the date `workerd --version` prints for WorkerdPin
// (1.YYYYMMDD.N → YYYY-MM-DD).
func WorkerdPinDate() string {
	d := WorkerdPin[2:10]
	return d[0:4] + "-" + d[4:6] + "-" + d[6:8]
}
