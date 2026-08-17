import unittest
from unittest.mock import patch

import app


class RestartTimeoutTests(unittest.TestCase):
    @patch("app._wait_for_inference", return_value={"pid": 123})
    @patch("app._checked_sudo")
    def test_restart_wait_exceeds_systemd_graceful_stop_window(self, checked, wait):
        result = app.restart_llama_server("/data/models/model.gguf", "vulkan")

        checked.assert_called_once_with(
            ["systemctl", "restart", "inference-server"],
            timeout=180,
        )
        wait.assert_called_once_with("/data/models/model.gguf", "vulkan")
        self.assertEqual(result, {"pid": 123})


if __name__ == "__main__":
    unittest.main()
