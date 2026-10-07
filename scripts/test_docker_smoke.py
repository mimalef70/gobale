import subprocess
import unittest
from unittest.mock import Mock, patch

import docker_smoke


class DockerPortReadinessTest(unittest.TestCase):
    def test_delayed_mapping_and_transient_docker_error(self):
        docker = Mock(side_effect=["", subprocess.CalledProcessError(1, ["docker", "port"]), "127.0.0.1:31000"])
        with patch.object(docker_smoke.time, "monotonic", return_value=0), patch.object(docker_smoke.time, "sleep") as sleep:
            self.assertEqual("31000", docker_smoke.wait_mapped_port(docker, "synthetic"))
        self.assertEqual(3, docker.call_count)
        self.assertEqual(2, sleep.call_count)

    def test_missing_or_invalid_mapping_has_bounded_deadline(self):
        for value in ("", "127.0.0.1:0", "127.0.0.1:99999", "not a port"):
            with self.subTest(mapping=value):
                docker = Mock(return_value=value)
                with patch.object(docker_smoke.time, "monotonic", side_effect=[0, 0, 31]), patch.object(docker_smoke.time, "sleep"):
                    with self.assertRaisesRegex(RuntimeError, "within 30 seconds"):
                        docker_smoke.wait_mapped_port(docker, "synthetic")
                docker.assert_called_once_with("port", "synthetic", "3000/tcp")


if __name__ == "__main__":
    unittest.main()
