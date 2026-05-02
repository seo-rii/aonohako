#!/usr/bin/env python3
import json
import pathlib
import sys

from graphql import build_schema, graphql_sync


def main() -> int:
    if len(sys.argv) != 2:
        print("usage: aonohako-graphql-run <query.graphql>", file=sys.stderr)
        return 2

    query_path = pathlib.Path(sys.argv[1])
    root = query_path.parent
    schema_path = root / "schema.graphql"
    schema_text = (
        schema_path.read_text(encoding="utf-8")
        if schema_path.exists()
        else "type Query { ok: String!, answer: Int! }\nschema { query: Query }\n"
    )
    query_text = query_path.read_text(encoding="utf-8")

    lowered = query_text.lower()
    if "mutation" in lowered or "subscription" in lowered or "__schema" in lowered:
        print("restricted GraphQL operation rejected", file=sys.stderr)
        return 1

    schema = build_schema(schema_text)
    root_value = {"ok": "ok", "answer": 42}
    result = graphql_sync(schema, query_text, root_value=root_value)
    if result.errors:
        for error in result.errors:
            print(error, file=sys.stderr)
        return 1
    print(json.dumps({"data": result.data}, separators=(",", ":"), sort_keys=True))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
