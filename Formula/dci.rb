class Dci < Formula
  desc "DoiT Cloud Intelligence CLI"
  homepage "https://github.com/doitintl/dci-cli"
  version "1.4.2"

  on_macos do
    if Hardware::CPU.arm?
      url "https://github.com/doitintl/dci-cli/releases/download/v1.4.2/dci_1.4.2_darwin_arm64.tar.gz"
      sha256 "7ad79ce3d3de491f5e00b0308b753ddacccb15daf20ca023703dbe6e0b9acaf0"
    else
      url "https://github.com/doitintl/dci-cli/releases/download/v1.4.2/dci_1.4.2_darwin_amd64.tar.gz"
      sha256 "e0623b58d0c66565011b4af198a6c7c900e978a9c292488e1ac55862233f6c94"
    end
  end

  on_linux do
    if Hardware::CPU.arm?
      url "https://github.com/doitintl/dci-cli/releases/download/v1.4.2/dci_1.4.2_linux_arm64.tar.gz"
      sha256 "6288f4703a90c9fb9a0a71cb7890b4d32eb9b98c65ee43b0be169f109c6576d5"
    else
      url "https://github.com/doitintl/dci-cli/releases/download/v1.4.2/dci_1.4.2_linux_amd64.tar.gz"
      sha256 "be67e4b0e577aa07539ef00f316f2f423be0529037c4ef1d5753f4f2475e7a94"
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
