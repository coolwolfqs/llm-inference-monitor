import tempfile
import threading
import time
import unittest
from pathlib import Path

from mm_core.runtime import SingleFlightTTLCache, service_pids


class RuntimeDiscoveryTests(unittest.TestCase):
    def test_existing_empty_cgroup_means_stopped_without_fallback(self):
        with tempfile.TemporaryDirectory() as tmp:
            path = Path(tmp) / "cgroup.procs"
            path.write_text("")
            self.assertEqual(service_pids(path), [])

    def test_missing_cgroup_is_explicitly_unavailable(self):
        self.assertIsNone(service_pids(Path("/definitely/missing/cgroup.procs")))

    def test_concurrent_miss_runs_one_loader(self):
        calls = 0
        calls_lock = threading.Lock()

        def loader():
            nonlocal calls
            with calls_lock:
                calls += 1
            time.sleep(0.05)
            return {"pid": 123}

        cache = SingleFlightTTLCache(loader, 10)
        barrier = threading.Barrier(20)
        results = []

        def worker():
            barrier.wait()
            results.append(cache.get())

        threads = [threading.Thread(target=worker) for _ in range(20)]
        for thread in threads:
            thread.start()
        for thread in threads:
            thread.join()
        self.assertEqual(calls, 1)
        self.assertEqual(len(results), 20)
        self.assertTrue(all(item["pid"] == 123 for item in results))

    def test_none_is_a_cacheable_stopped_state(self):
        calls = 0

        def loader():
            nonlocal calls
            calls += 1
            return None

        cache = SingleFlightTTLCache(loader, 10)
        self.assertIsNone(cache.get())
        self.assertIsNone(cache.get())
        self.assertEqual(calls, 1)

    def test_reader_gets_stale_value_while_forced_refresh_runs(self):
        started = threading.Event()
        release = threading.Event()
        calls = 0

        def loader():
            nonlocal calls
            calls += 1
            if calls > 1:
                started.set()
                release.wait(timeout=2)
            return calls

        cache = SingleFlightTTLCache(loader, 60)
        self.assertEqual(cache.get(), 1)
        refresher = threading.Thread(target=lambda: cache.get(force=True))
        refresher.start()
        self.assertTrue(started.wait(timeout=1))
        before = time.perf_counter()
        self.assertEqual(cache.get(), 1)
        self.assertLess(time.perf_counter() - before, 0.1)
        release.set()
        refresher.join(timeout=1)
        self.assertEqual(cache.get(), 2)

    def test_peek_never_loads(self):
        cache = SingleFlightTTLCache(lambda: 7, 60)
        self.assertEqual(cache.peek(), (False, None))
        self.assertEqual(cache.get(), 7)
        self.assertEqual(cache.peek(), (True, 7))


if __name__ == "__main__":
    unittest.main()
