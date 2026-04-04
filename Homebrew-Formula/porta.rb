class Porta < Formula
  desc "Portable automation runtime with USB triggers, hooks, and offline-first execution"
  homepage "https://github.com/rafalmasiarek/porta"
  url "https://github.com/rafalmasiarek/porta/releases/download/v4.5.1/porta-darwin-amd64"
  sha256 "57465bb1e41e1e1bda89db490ea5035f13b742855c047132dc35241579d6eb6f"
  version "4.5.1"

  def install
    bin.install "porta-darwin-amd64" => "porta"
  end
end
