class Dci < Formula
  desc "DoiT Cloud Intelligence CLI"
  homepage "https://github.com/doitintl/dci-cli"
  version "1.4.1"

  on_macos do
    if Hardware::CPU.arm?
      url "https://github.com/doitintl/dci-cli/releases/download/v1.4.1/dci_1.4.1_darwin_arm64.tar.gz"
      sha256 "32e160336bc572a4bf1cd14aad617b5b9f1d38fc26768dd5cb7892b1bccf7946"
    else
      url "https://github.com/doitintl/dci-cli/releases/download/v1.4.1/dci_1.4.1_darwin_amd64.tar.gz"
      sha256 "e325af2858283eddf9bf3eacd8def8b6c3415c05f509f07121e974e564a078fb"
    end
  end

  on_linux do
    if Hardware::CPU.arm?
      url "https://github.com/doitintl/dci-cli/releases/download/v1.4.1/dci_1.4.1_linux_arm64.tar.gz"
      sha256 "2f0dda40fac97847e8543a8e0593d299cca4b3b7e145becaefcc0f504bb5f85a"
    else
      url "https://github.com/doitintl/dci-cli/releases/download/v1.4.1/dci_1.4.1_linux_amd64.tar.gz"
      sha256 "f3f4a940a013cb8beb32c50e4d67f684354d7f65f113cbfe30337980a1a7b0a0"
    end
  end

  def install
    bin.install "dci"
  end

  test do
    output = shell_output("#{bin}/dci --help")
    assert_match "DoiT Cloud Intelligence", output
  end
end
