class Stash < Formula
  desc "Personal knowledge vault — capture, organize, and search anything"
  homepage "https://github.com/msjurset/gostash"
  license "MIT"
  head "https://github.com/msjurset/gostash.git", branch: "main"

  depends_on "go" => :build

  def install
    ldflags = "-s -w -X main.version=#{version}"
    system "go", "generate", "./internal/manpage/"
    system "go", "build", *std_go_args(ldflags:), "-trimpath", "-o", bin/"stash", "./cmd/stash"

    man1.install "stash.1"
    bash_completion.install "completions/stash.bash" => "stash"
    zsh_completion.install "completions/stash.zsh" => "_stash"
  end

  test do
    assert_match "stash", shell_output("#{bin}/stash --version")
  end
end
