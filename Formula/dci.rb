class Dci < Formula
  desc "Cloud Intelligence™ CLI"
  homepage "https://github.com/doitintl/dci-cli"
  version "2.6.0"

  on_macos do
    if Hardware::CPU.arm?
      url "https://github.com/doitintl/dci-cli/releases/download/v2.6.0/dci_2.6.0_darwin_arm64.tar.gz"
      sha256 "6813c840ae9e9a9903394ab6551dd0ed64f0fdf2c62015e1fcfcb910ecc27fbf"
    else
      url "https://github.com/doitintl/dci-cli/releases/download/v2.6.0/dci_2.6.0_darwin_amd64.tar.gz"
      sha256 "9e93e412c808f4515d107fc41b826a86a248de8da3663edd7da8373ebcfa42be"
    end
  end

  on_linux do
    if Hardware::CPU.arm?
      url "https://github.com/doitintl/dci-cli/releases/download/v2.6.0/dci_2.6.0_linux_arm64.tar.gz"
      sha256 "fd1a7b3205fc99906a193daf8d04e7a920a11c5350f4947612e1a059a17443ac"
    else
      url "https://github.com/doitintl/dci-cli/releases/download/v2.6.0/dci_2.6.0_linux_amd64.tar.gz"
      sha256 "90e1d3cd88c6b50c428cbd9e9e5d58d5199508023cbec9a5783ceeeaacc3246b"
    end
  end

  def install
    bin.install "dci"
    bash_completion.install "completions/dci.bash" => "dci"
    zsh_completion.install "completions/dci.zsh" => "_dci"
    fish_completion.install "completions/dci.fish"
  end

  test do
    output = shell_output("#{bin}/dci --help")
    assert_match "Cloud Intelligence™", output
  end
end
