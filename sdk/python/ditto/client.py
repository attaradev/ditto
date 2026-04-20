"""Ditto Python client — pure stdlib, no external dependencies."""

import json
import os
import urllib.error
import urllib.request
from contextlib import contextmanager
from typing import Generator


class DittoError(Exception):
    """Raised when the ditto host returns an error or is unreachable."""


class Client:
    """Provisions ephemeral database copies from a running ditto host.

    Configuration can be supplied via constructor arguments or environment
    variables. Environment variables take precedence when both are present:

        DITTO_SERVER_URL  — base URL of the ditto host (required)
        DITTO_TOKEN       — Bearer token (typically an OIDC JWT)
        DITTO_TTL         — default copy lifetime in seconds (integer string)

    Examples::

        client = Client()  # reads from environment variables

        client = Client(
            server_url="http://ditto.internal:8080",
            token=os.environ["DITTO_TOKEN"],
            ttl_seconds=3600,
        )
    """

    def __init__(
        self,
        server_url: str = "",
        token: str = "",
        ttl_seconds: int = 0,
    ) -> None:
        self._base_url = (os.environ.get("DITTO_SERVER_URL") or server_url).rstrip("/")
        self._token = os.environ.get("DITTO_TOKEN") or token
        ttl_env = os.environ.get("DITTO_TTL")
        self._ttl_seconds = int(ttl_env) if ttl_env else (ttl_seconds or 0)

        if not self._base_url:
            raise DittoError(
                "server_url is required — pass it as an argument or set DITTO_SERVER_URL"
            )

    # ------------------------------------------------------------------
    # Public API
    # ------------------------------------------------------------------

    def create(
        self,
        ttl_seconds: int = 0,
        run_id: str = "",
        job_name: str = "",
        dump_uri: str = "",
        obfuscate: bool = False,
    ) -> dict:
        """Create a new ephemeral database copy.

        Returns a dict with at minimum the keys ``id`` and
        ``connection_string``.

        Args:
            ttl_seconds: Override the default copy lifetime in seconds.
            run_id: Optional run/session identifier for auditing.
            job_name: Optional job/step identifier for auditing.
            dump_uri: Optional dump URI or host-local dump path resolved by the server.
            obfuscate: Request post-restore obfuscation on the created copy.

        Raises:
            DittoError: If the server returns an error.
        """
        body: dict = {}
        effective_ttl = ttl_seconds or self._ttl_seconds
        if effective_ttl:
            body["ttl_seconds"] = effective_ttl
        if run_id:
            body["run_id"] = run_id
        if job_name:
            body["job_name"] = job_name
        if dump_uri:
            body["dump_uri"] = dump_uri
        if obfuscate:
            body["obfuscate"] = True
        result = self._request("POST", "/v2/copies", body)
        assert result is not None  # POST /v2/copies always returns a body
        return result

    def destroy(self, copy_id: str) -> None:
        """Destroy a copy by ID.

        Args:
            copy_id: The copy ID returned by :meth:`create`.

        Raises:
            DittoError: If the server returns an error.
        """
        self._request("DELETE", f"/v2/copies/{copy_id}")

    def list(self) -> list[dict]:
        """Return all copies known to the server.

        Raises:
            DittoError: If the server returns an error.
        """
        result = self._request("GET", "/v2/copies")
        return result if result is not None else []

    def events(self, copy_id: str) -> list[dict]:
        """Return lifecycle events for a copy by ID."""
        result = self._request("GET", f"/v2/copies/{copy_id}/events")
        return result if result is not None else []

    def status(self) -> dict:
        """Return shared-host status. Requires an admin-capable bearer token."""
        result = self._request("GET", "/v2/status")
        return result if isinstance(result, dict) else {}

    @contextmanager
    def with_copy(
        self,
        ttl_seconds: int = 0,
        run_id: str = "",
        job_name: str = "",
    ) -> Generator[str, None, None]:
        """Context manager that creates a copy, yields its DSN, then destroys it.

        The copy is destroyed even if the body raises an exception.

        Args:
            ttl_seconds: Override the default copy lifetime in seconds.
            run_id: Optional run identifier for auditing.
            job_name: Optional job identifier for auditing.

        Yields:
            The database connection string (DSN) for the copy.

        Example::

            with client.with_copy() as dsn:
                conn = psycopg2.connect(dsn)
                cur = conn.cursor()
                cur.execute("SELECT 1")
        """
        copy = self.create(ttl_seconds=ttl_seconds, run_id=run_id, job_name=job_name)
        try:
            yield copy["connection_string"]
        finally:
            try:
                self.destroy(copy["id"])
            except DittoError:
                pass  # best-effort; copy will expire via TTL

    # ------------------------------------------------------------------
    # Internal helpers
    # ------------------------------------------------------------------

    def _headers(self) -> dict[str, str]:
        h: dict[str, str] = {"Content-Type": "application/json"}
        if self._token:
            h["Authorization"] = f"Bearer {self._token}"
        return h

    def _request(
        self, method: str, path: str, body: dict | None = None
    ) -> dict | list | None:
        url = self._base_url + path
        data = json.dumps(body).encode() if body is not None else None
        req = urllib.request.Request(
            url, data=data, headers=self._headers(), method=method
        )
        try:
            with urllib.request.urlopen(req) as resp:
                raw = resp.read()
                if raw:
                    return json.loads(raw)
                return None
        except urllib.error.HTTPError as exc:
            raw = exc.read()
            msg = ""
            try:
                msg = json.loads(raw).get("error", "")
            except Exception:
                pass
            raise DittoError(
                f"ditto: {method} {path} returned HTTP {exc.code}: {msg or raw.decode(errors='replace')}"
            ) from exc
        except urllib.error.URLError as exc:
            raise DittoError(
                f"ditto: {method} {path} failed: {exc.reason}"
            ) from exc
