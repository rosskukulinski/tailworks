// skewclock: an LD_PRELOAD shim for tests only (internal/wallclock's test
// hook; e2e/cliflow.BuildClockPreload compiles it). It shifts a process's
// wall clock (CLOCK_REALTIME and its variants, gettimeofday, time) by the
// whole seconds in the file named by $TW_TEST_CLOCK_FILE ("+300", "-90"),
// re-read at most every 100 ms, so a test can step it while the process
// runs. Monotonic clocks are untouched: a wrong wall clock doesn't make a
// machine's timers run fast or slow.
//
// Linux only. It makes raw system calls rather than finding the real
// functions with dlsym, which can deadlock while a process starts
// (libfaketime hangs workerd that way).
#define _GNU_SOURCE
#include <errno.h>
#include <fcntl.h>
#include <stdlib.h>
#include <sys/syscall.h>
#include <sys/time.h>
#include <time.h>
#include <unistd.h>

static long long off_s;        // the offset, seconds
static long long next_read_ns; // CLOCK_MONOTONIC ns of the next re-read

static long long offset_s(void) {
	struct timespec m;
	syscall(SYS_clock_gettime, CLOCK_MONOTONIC, &m);
	long long now = (long long)m.tv_sec * 1000000000LL + m.tv_nsec;
	if (now >= __atomic_load_n(&next_read_ns, __ATOMIC_RELAXED)) {
		__atomic_store_n(&next_read_ns, now + 100000000LL, __ATOMIC_RELAXED);
		const char *p = getenv("TW_TEST_CLOCK_FILE");
		if (p) {
			int fd = (int)syscall(SYS_openat, AT_FDCWD, p, O_RDONLY | O_CLOEXEC);
			if (fd >= 0) {
				char buf[32];
				long n = syscall(SYS_read, fd, buf, sizeof buf - 1);
				syscall(SYS_close, fd);
				if (n > 0) {
					buf[n] = 0;
					char *end;
					long long v = strtoll(buf, &end, 10);
					if (end != buf)
						__atomic_store_n(&off_s, v, __ATOMIC_RELAXED);
				}
			}
		}
	}
	return __atomic_load_n(&off_s, __ATOMIC_RELAXED);
}

int clock_gettime(clockid_t id, struct timespec *ts) {
	int saved = errno;
	if (syscall(SYS_clock_gettime, id, ts) != 0)
		return -1;
	if (id == CLOCK_REALTIME || id == CLOCK_REALTIME_COARSE || id == CLOCK_TAI)
		ts->tv_sec += offset_s();
	errno = saved;
	return 0;
}

int gettimeofday(struct timeval *restrict tv, void *restrict tz) {
	struct timespec ts;
	(void)tz;
	if (clock_gettime(CLOCK_REALTIME, &ts) != 0)
		return -1;
	tv->tv_sec = ts.tv_sec; // glibc declares tv nonnull
	tv->tv_usec = ts.tv_nsec / 1000;
	return 0;
}

time_t time(time_t *t) {
	struct timespec ts;
	clock_gettime(CLOCK_REALTIME, &ts);
	if (t)
		*t = ts.tv_sec;
	return ts.tv_sec;
}
