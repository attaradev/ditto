"""Pytest plugin for ditto — auto-registered via the pytest11 entry point.

When ``ditto-sdk[pytest]`` is installed, this plugin is discovered
automatically by pytest. No ``conftest.py`` changes are needed.

Fixtures
--------
ditto_client : session-scoped
    A :class:`~ditto.client.Client` instance configured from environment
    variables (``DITTO_SERVER_URL``, ``DITTO_TOKEN``, ``DITTO_TTL``).

ditto_copy : function-scoped
    Creates an ephemeral database copy before the test and destroys it
    afterwards. Yields the connection string (DSN).

Examples
--------
::

    def test_user_insert(ditto_copy):
        conn = psycopg2.connect(ditto_copy)
        cur = conn.cursor()
        cur.execute("INSERT INTO users (email) VALUES (%s)", ("test@example.com",))
        conn.commit()
        cur.execute("SELECT COUNT(*) FROM users")
        assert cur.fetchone()[0] == 1
"""

import pytest

from ditto.client import Client, DittoError


@pytest.fixture(scope="session")
def ditto_client() -> Client:
    """Session-scoped ditto Client configured from environment variables.

    Requires ``DITTO_SERVER_URL`` to be set. Optionally reads
    ``DITTO_TOKEN`` and ``DITTO_TTL``.
    """
    return Client()


@pytest.fixture
def ditto_copy(ditto_client: Client) -> object:
    """Function-scoped fixture that yields a copy DSN and auto-destroys.

    The copy is destroyed in a ``finally`` block so cleanup happens even
    when the test raises an exception.
    """
    copy = ditto_client.create()
    yield copy["connection_string"]
    try:
        ditto_client.destroy(copy["id"])
    except DittoError:
        pass  # best-effort; copy will expire via TTL
