# stow-s3

A disposable, fully isolated S3 endpoint for tests, from Python.

`stow-s3` starts a native stow server as a child process, gives you an endpoint
and credentials, and guarantees the server is gone when you are done. Nothing
touches a real bucket, and nothing is left running between tests.

## Install

```sh
pip install stow-s3
```

The native binary ships inside a platform wheel, so there is no `PATH` entry to
set, no binary to download, and no Go toolchain to install.

`boto3` is optional. Install it if you want the built-in client helper:

```sh
pip install "stow-s3[boto3]"
```

## Use

```python
from stow_s3 import with_session

def test_my_handler():
    with with_session() as session:
        s3 = session.s3_client()
        bucket = session.new_bucket_name()
        s3.create_bucket(Bucket=bucket)

        s3.put_object(Bucket=bucket, Key="input.json", Body=b'{"task":"summarize"}')

        result = my_handler(s3, bucket)  # a normal boto3 client
        assert result == "summarize"
```

When the block ends, the server stops and its data directory is removed. If your
test fails, cleanup happens anyway.

`with_session` takes limits that the server then enforces on every request, so a
test that accidentally writes too much fails instead of consuming the disk:

```python
with with_session(max_bytes=1024 * 1024, max_objects=10) as session:
    ...
```

The defaults are 16 MiB and 1000 objects, which came from measurement rather
than taste. See `docs/benchmarks/session-baseline.md`.

## Without boto3

A session performs no S3 I/O of its own. It reports the endpoint and credentials
and hands back a client, so any S3 library will work:

```python
import boto3  # or whatever you prefer

with with_session() as session:
    s3 = boto3.client(
        "s3",
        endpoint_url=session.endpoint,
        aws_access_key_id=session.access_key_id,
        aws_secret_access_key=session.secret_access_key,
        region_name=session.region,
    )
```

## Isolation

A session does not inherit `STOW_*`, `S3_*` or `AWS_*` from your shell, so a
stray credential cannot silently turn a local test into one that writes to a real
bucket.

Credentials never appear on the child's stdout. They arrive over a versioned
readiness channel, and a client that does not understand the protocol version
raises `StowProtocolError` rather than guessing.

## When something does not work

Run the server's own diagnostic. It reports whether a binary exists for this
platform, whether the temporary directory is writable, whether a backend opens,
and whether a real endpoint can bind and answer a health check:

```sh
stow doctor
stow doctor --json
```

If the Python package cannot find a binary at all, `StowBinaryNotFoundError`
names the resolution order it used.

## License

Apache-2.0
