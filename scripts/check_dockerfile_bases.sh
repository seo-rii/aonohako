#!/usr/bin/env bash
set -euo pipefail

if [[ "$#" -eq 0 ]]; then
  echo "usage: check_dockerfile_bases.sh <dockerfile>..." >&2
  exit 2
fi

for file in "$@"; do
  if [[ ! -f "${file}" ]]; then
    echo "dockerfile base policy failed: missing ${file}" >&2
    exit 1
  fi

  declare -A global_args=()
  declare -A stages=()
  saw_from=false
  parser_directives_allowed=true
  escape_directive_seen=false
  escape_char='\'
  logical_line=""
  instruction_line=0
  line_number=0
  parser_directive_pattern='^#[[:space:]]*([a-z]+)[[:space:]]*=[[:space:]]*([^[:space:]]+)[[:space:]]*$'
  while IFS= read -r raw_line || [[ -n "${raw_line}" ]]; do
    line_number=$((line_number + 1))
    raw_line="${raw_line%$'\r'}"
    trimmed_line="${raw_line#"${raw_line%%[![:space:]]*}"}"

    if [[ -z "${logical_line}" && "${parser_directives_allowed}" == true ]]; then
      lowered_line="${trimmed_line,,}"
      if [[ "${lowered_line}" =~ ${parser_directive_pattern} ]]; then
        directive_name="${BASH_REMATCH[1]}"
        directive_value="${BASH_REMATCH[2]}"
        if [[ "${directive_name}" == "escape" ]]; then
          if [[ "${escape_directive_seen}" == true ]]; then
            echo "dockerfile base policy failed: ${file}:${line_number} repeats the escape parser directive" >&2
            exit 1
          fi
          if [[ "${directive_value}" != '\' && "${directive_value}" != '`' ]]; then
            echo "dockerfile base policy failed: ${file}:${line_number} has unsupported escape character ${directive_value@Q}" >&2
            exit 1
          fi
          escape_char="${directive_value}"
          escape_directive_seen=true
        fi
        continue
      fi
      if [[ -z "${trimmed_line}" || "${trimmed_line}" == \#* ]]; then
        parser_directives_allowed=false
        continue
      fi
      parser_directives_allowed=false
    fi

    if [[ -z "${trimmed_line}" || "${trimmed_line}" == \#* ]]; then
      continue
    fi

    if [[ -z "${logical_line}" ]]; then
      instruction_line="${line_number}"
      logical_line="${raw_line}"
    else
      logical_line+="${trimmed_line}"
    fi
    continuation_line="${logical_line}"
    while [[ "${continuation_line}" == *' ' || "${continuation_line}" == *$'\t' ]]; do
      continuation_line="${continuation_line:0:${#continuation_line}-1}"
    done
    if [[ "${continuation_line: -1}" == "${escape_char}" ]]; then
      logical_line="${continuation_line:0:${#continuation_line}-1}"
      continue
    fi

    read -r -a tokens <<< "${logical_line}"
    logical_line=""
    if [[ "${#tokens[@]}" -eq 0 ]]; then
      continue
    fi

    directive="${tokens[0],,}"
    if [[ "${directive}" == "arg" && "${saw_from}" == false && "${#tokens[@]}" -ge 2 ]]; then
      declaration="${tokens[1]}"
      name="${declaration%%=*}"
      value=""
      if [[ "${declaration}" == *=* ]]; then
        value="${declaration#*=}"
      fi
      global_args["${name}"]="${value}"
      continue
    fi
    if [[ "${directive}" != "from" ]]; then
      continue
    fi

    saw_from=true
    index=1
    while [[ "${index}" -lt "${#tokens[@]}" && "${tokens[${index}]}" == --platform=* ]]; do
      index=$((index + 1))
    done
    if [[ "${index}" -ge "${#tokens[@]}" ]]; then
      echo "dockerfile base policy failed: ${file}:${instruction_line} has no FROM source" >&2
      exit 1
    fi

    source="${tokens[${index}]}"
    source_key="${source,,}"
    if [[ "${source_key}" != "scratch" && -z "${stages[${source_key}]:-}" ]]; then
      arg_name=""
      if [[ "${source}" =~ ^\$\{([A-Za-z_][A-Za-z0-9_]*)\}$ ]]; then
        arg_name="${BASH_REMATCH[1]}"
      elif [[ "${source}" =~ ^\$([A-Za-z_][A-Za-z0-9_]*)$ ]]; then
        arg_name="${BASH_REMATCH[1]}"
      fi

      if [[ -n "${arg_name}" ]]; then
        arg_value="${global_args[${arg_name}]:-}"
        if [[ ! "${arg_value}" =~ @sha256:[0-9a-f]{64}$ ]]; then
          echo "dockerfile base policy failed: ${file}:${instruction_line} FROM uses unpinned ARG ${arg_name}" >&2
          exit 1
        fi
      elif [[ ! "${source}" =~ @sha256:[0-9a-f]{64}$ ]]; then
        echo "dockerfile base policy failed: ${file}:${instruction_line} has unpinned external FROM ${source}" >&2
        exit 1
      fi
    fi

    index=$((index + 1))
    while [[ "${index}" -lt "${#tokens[@]}" ]]; do
      if [[ "${tokens[${index}],,}" == "as" && $((index + 1)) -lt "${#tokens[@]}" ]]; then
        stages["${tokens[$((index + 1))],,}"]=1
        break
      fi
      index=$((index + 1))
    done
  done < "${file}"
  if [[ -n "${logical_line}" ]]; then
    echo "dockerfile base policy failed: ${file}:${instruction_line} has an unterminated escape continuation" >&2
    exit 1
  fi
  if [[ "${saw_from}" == false ]]; then
    echo "dockerfile base policy failed: ${file} contains no FROM instruction" >&2
    exit 1
  fi
  unset global_args stages
done

echo "verified digest-pinned Dockerfile bases in $# file(s)"
