"""Capture all stdout for the long panel using the original v12 collector."""
import json


def with_capture(body):
    request = json.loads(body)
    request["capture_limits"] = {"stdout_bytes": 1 << 20}
    return json.dumps(request, separators=(",", ":")).encode()


if __name__ == "__main__":
    import base_guest

    original_body = base_guest.make_body

    def make_body(case, kind, root):
        return with_capture(original_body(case, kind, root))

    base_guest.make_body = make_body
    base_guest.main()
