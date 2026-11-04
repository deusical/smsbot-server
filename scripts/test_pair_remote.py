"""Offline checks for the fixed remote program embedded in the PowerShell helper."""
import io
import pathlib
import subprocess
import unittest
from unittest import mock


PROGRAM = (pathlib.Path(__file__).with_name("pair-remote.ps1").read_text()
           .split("$program = @'\n", 1)[1].split("\n'@", 1)[0])


class RemotePairingTests(unittest.TestCase):
    def run_program(self, pair_result=0, pair_error=None, payload=b"synthetic-cookies"):
        calls = []

        def run(args, **kwargs):
            calls.append((args, kwargs))
            if "--pair-new-phone" in args:
                if pair_error:
                    raise pair_error
                return subprocess.CompletedProcess(args, pair_result)
            return subprocess.CompletedProcess(args, 0)

        with mock.patch("sys.stdin", new=mock.Mock(buffer=io.BytesIO(payload))), \
             mock.patch("pathlib.Path.is_file", return_value=True), \
             mock.patch("subprocess.run", side_effect=run):
            try:
                exec(compile(PROGRAM, "remote-pairing", "exec"), {})
            except SystemExit as error:
                return calls, error.code
            except subprocess.TimeoutExpired:
                return calls, "timeout"
        self.fail("program should exit")

    def test_credentials_only_on_stdin_and_restart_on_success(self):
        calls, code = self.run_program()
        self.assertEqual(code, 0)
        self.assertEqual(calls[0][0], ["systemctl", "--user", "stop", "smsbot"])
        self.assertEqual(calls[1][1]["input"], b"synthetic-cookies")
        self.assertNotIn("synthetic-cookies", " ".join(calls[1][0]))
        self.assertEqual(calls[-1][0], ["systemctl", "--user", "start", "smsbot"])

    def test_failed_pairing_restarts_and_propagates_exit_code(self):
        calls, code = self.run_program(pair_result=1)
        self.assertEqual(code, 1)
        self.assertEqual(calls[-1][0][-2:], ["start", "smsbot"])

    def test_timeout_still_restarts(self):
        calls, code = self.run_program(pair_error=subprocess.TimeoutExpired("synthetic", 200))
        self.assertEqual(code, "timeout")
        self.assertEqual(calls[-1][0][-2:], ["start", "smsbot"])

    def test_oversized_input_never_stops_service(self):
        calls, _ = self.run_program(payload=b"s" * 32769)
        self.assertEqual(calls, [])


if __name__ == "__main__":
    unittest.main()
