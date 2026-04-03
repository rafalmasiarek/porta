class Porta < Formula
  desc "Portable automation runtime with USB triggers, hooks, and offline-first execution"
  homepage "https://github.com/rafalmasiarek/porta"
  url "https://github.com/rafalmasiarek/porta/releases/download/v4.2.0/porta-darwin-amd64"
  sha256 "8a0bca9e260832fee4d66518a5ab9b05b9e02a76eba7c22df007a028103f1e4c"
  version "4.2.0"

  def install
    bin.install "porta-darwin-amd64" => "porta"
  end
end
