#!/usr/bin/env ruby
# frozen_string_literal: true

path = ARGV.fetch(0) { abort "usage: golfscript_sandboxed.rb <program.gs>" }
code = File.read(path, mode: "rb", encoding: Encoding::UTF_8)

if code.match?(/[`\$;]|system|exec|eval|IO|File|Dir|Kernel/)
  warn "restricted GolfScript token rejected"
  exit 1
end

rest = code.dup
until rest.empty?
  rest = rest.lstrip
  break if rest.empty?

  if rest.start_with?('"')
    token = +""
    escaped = false
    i = 1
    while i < rest.length
      ch = rest[i]
      if escaped
        token << case ch
                 when "n" then "\n"
                 when "t" then "\t"
                 else ch
                 end
        escaped = false
      elsif ch == "\\"
        escaped = true
      elsif ch == '"'
        print token
        rest = rest[(i + 1)..] || ""
        token = nil
        break
      else
        token << ch
      end
      i += 1
    end
    abort "unterminated string" unless token.nil?
  else
    abort "unsupported sandboxed GolfScript token near: #{rest[0, 16].inspect}"
  end
end
