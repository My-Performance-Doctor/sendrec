"""Synthetic container-start checks, no database or external downloads."""
import hashlib
import os
from pathlib import Path
import subprocess
import tempfile
import unittest
from urllib.parse import unquote, urlsplit

ENTRYPOINT = Path(__file__).resolve().parents[2] / "docker-entrypoint.sh"
MODEL = b"synthetic whisper model"


class EntrypointTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name)
        self.env = {"PATH": os.environ["PATH"], "GARAGE_KEYS_FILE": str(self.root / "absent")}
        self.env.update(DB_HOST="database.example.test", DB_USER="synthetic+user", DB_PASSWORD="synthetic:/@%$'secret", DB_NAME="fixture/database")

    def run_entrypoint(self, extra=None, inspect=False):
        env = self.env | (extra or {})
        command = ["python3", "-c", "import os; print(os.environ['DATABASE_URL'])"] if inspect else ["sh", "-c", "printf started"]
        return subprocess.run(["sh", str(ENTRYPOINT), *command], env=env, text=True, capture_output=True)

    def test_database_fields_escape_reserved_characters(self):
        result = self.run_entrypoint(inspect=True)
        self.assertEqual(result.returncode, 0, result.stderr)
        parsed = urlsplit(result.stdout.strip())
        self.assertEqual(unquote(parsed.username), self.env["DB_USER"])
        self.assertEqual(unquote(parsed.password), self.env["DB_PASSWORD"])
        self.assertEqual(unquote(parsed.path[1:]), self.env["DB_NAME"])
        self.assertEqual(parsed.query, "sslmode=require")

    def test_compose_url_passes_through(self):
        url = "postgres://fixture:synthetic@localhost/fixture?sslmode=disable"
        self.assertEqual(self.run_entrypoint({"DATABASE_URL": url, "DB_PASSWORD": ""}, inspect=True).stdout.strip(), url)

    def test_invalid_database_fields_fail_without_exposing_values(self):
        for fields in [{"DB_PASSWORD": ""}, {"DB_PORT": "12x"}, {"DB_HOST": "unsafe@host"}, {"DB_SSLMODE": "require&unexpected=yes"}]:
            result = self.run_entrypoint(fields)
            self.assertNotEqual(result.returncode, 0)
            self.assertNotIn("started", result.stdout)
            self.assertNotIn(self.env["DB_PASSWORD"], result.stdout + result.stderr)

    def model_env(self):
        return dict(TRANSCRIPTION_ENABLED="true", WHISPER_MODEL_URL="https://model.example.test/pinned", WHISPER_MODEL_PATH=str(self.root / "model.bin"), WHISPER_MODEL_SHA256=hashlib.sha256(MODEL).hexdigest())

    def fake_download(self, fail=False, corrupt=False):
        wget = self.root / "wget"
        wget.write_text("#!/bin/sh\n" + ("exit 1\n" if fail else "while [ \"$1\" != -O ]; do shift; done\nprintf '%s' '" + ("corrupt" if corrupt else MODEL.decode()) + "' > \"$2\"\n"))
        wget.chmod(0o755)
        self.env["PATH"] = str(self.root) + ":" + self.env["PATH"]

    def test_missing_model_settings_fail(self):
        result = self.run_entrypoint(self.model_env() | {"WHISPER_MODEL_SHA256": ""})
        self.assertNotEqual(result.returncode, 0)
        self.assertNotIn("started", result.stdout)

    def test_model_download_verified_and_cached(self):
        self.fake_download()
        env = self.model_env()
        self.assertEqual(self.run_entrypoint(env).returncode, 0)
        self.fake_download(fail=True)
        self.assertEqual(self.run_entrypoint(env).returncode, 0)
        self.assertEqual(Path(env["WHISPER_MODEL_PATH"]).read_bytes(), MODEL)

    def test_failed_or_corrupt_download_never_starts(self):
        for options in [{"fail": True}, {"corrupt": True}]:
            self.fake_download(**options)
            result = self.run_entrypoint(self.model_env())
            self.assertNotEqual(result.returncode, 0)
            self.assertFalse((self.root / "model.bin").exists())
            self.assertEqual(list(self.root.glob("*.part.*")), [])

    def test_corrupt_cache_fails(self):
        (self.root / "model.bin").write_bytes(b"corrupt")
        self.assertNotEqual(self.run_entrypoint(self.model_env()).returncode, 0)


if __name__ == "__main__":
    unittest.main()
