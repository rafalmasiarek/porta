class Porta < Formula
  desc "Portable automation runtime with USB triggers, hooks, and offline-first execution"
  homepage "https://github.com/rafalmasiarek/porta"
  url "https://github.com/rafalmasiarek/porta/releases/download/v4.4.2/porta-darwin-amd64"
  sha256 "f40d0559dbb23cd71b5625995c757f6058620eb05e55df6fe9950b48e4ae392f"
  version "4.4.2"

  def install
    bin.install "porta-darwin-amd64" => "porta"
  end
end
