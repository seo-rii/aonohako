#!/usr/bin/env ruby
# frozen_string_literal: true

path = ARGV.fetch(0) { abort "usage: aonohako-vb6-run <program.bas>" }

File.readlines(path, chomp: true).each do |line|
  stripped = line.strip
  next if stripped.empty?
  next if stripped.start_with?("'")
  next if stripped.match?(/\A(Option\s+Explicit|Sub\s+Main\(\)|End\s+Sub)\z/i)

  if (match = stripped.match(/\A(?:Debug\.)?Print\s+"(.*)"\z/i))
    puts match[1].gsub('\n', "\n")
    next
  end

  warn "unsupported VB6 console subset statement: #{stripped}"
  exit 1
end
