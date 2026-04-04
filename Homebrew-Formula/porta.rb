class Porta < Formula
  desc "Portable automation runtime with USB triggers, hooks, and offline-first execution"
  homepage "https://github.com/rafalmasiarek/porta"
  url "https://github.com/rafalmasiarek/porta/releases/download/v4.4.0/porta-darwin-amd64"
  sha256 "079c79b5dab23024c7694e4a16d38549b945225b1658dceaefc551d3735c1835"
  version "4.4.0"

  def install
    bin.install "porta-darwin-amd64" => "porta"
  end
end
