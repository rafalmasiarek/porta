class Porta < Formula
  desc "Portable automation runtime with USB triggers, hooks, and offline-first execution"
  homepage "https://github.com/rafalmasiarek/porta"
  url "https://github.com/rafalmasiarek/porta/releases/download/v4.5.0/porta-darwin-amd64"
  sha256 "219b704c96e8d102f36cb349440452f2f04e924d36709b104c210d7d63b69621"
  version "4.5.0"

  def install
    bin.install "porta-darwin-amd64" => "porta"
  end
end
