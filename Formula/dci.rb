class Dci < Formula
  desc "DoiT Cloud Intelligence CLI"
  homepage "https://github.com/doitintl/dci-cli"
  version "1.1.0"

  on_macos do
    if Hardware::CPU.arm?
      url "https://github.com/doitintl/dci-cli/releases/download/v1.1.0/dci_1.1.0_darwin_arm64.tar.gz"
      sha256 "ded5aea691a9dbbd12c670697f59f2705a1a186fd6d319a991977e804e7ead11"
    else
      url "https://github.com/doitintl/dci-cli/releases/download/v1.1.0/dci_1.1.0_darwin_amd64.tar.gz"
      sha256 "1c6a0c692ac592c287ef80a3af0418f9e0a6dfb16545d3aecfff34c37ce36347"
    end
  end

  on_linux do
    if Hardware::CPU.arm?
      url "https://github.com/doitintl/dci-cli/releases/download/v1.1.0/dci_1.1.0_linux_arm64.tar.gz"
      sha256 "349c0252a3caac37b99b30db89a68ed5df423c5e9f0d36a87b4f8eeb90a40767"
    else
      url "https://github.com/doitintl/dci-cli/releases/download/v1.1.0/dci_1.1.0_linux_amd64.tar.gz"
      sha256 "4e748582f01ba08055cdf1de87ad1ea4b9042c56ac641088d0a496dfaac02282"
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
