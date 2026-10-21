package sandbox

import (
	"context"
	"os/exec"
)

// BaseImage is a prebuilt image with the heavy, common dependencies of ML
// papers already installed (CPU-only PyTorch, NumPy, pandas, scikit-learn...).
// Installing these per paper dominates run time, so they are baked in once.
const BaseImage = "crucible-base:py311"

// BaseDockerfile builds BaseImage. Recorded Dockerfiles start FROM this image,
// so anyone can rebuild it with `crucible base`.
const BaseDockerfile = `FROM python:3.11
ENV PIP_DISABLE_PIP_VERSION_CHECK=1 PIP_PROGRESS_BAR=off PYTHONUNBUFFERED=1
RUN pip install --no-cache-dir torch --index-url https://download.pytorch.org/whl/cpu
RUN pip install --no-cache-dir numpy pandas scipy scikit-learn matplotlib einops tqdm pyyaml h5py
WORKDIR /work
`

// pipEnv makes pip fast and keeps it on CPU-only PyTorch: with the CPU index
// as an extra index, a "+cpu" build outranks the default CUDA build of the same
// version, so a repo's requirements.txt can't pull in multi-gigabyte GPU wheels.
var pipEnv = []string{
	"PIP_EXTRA_INDEX_URL=https://download.pytorch.org/whl/cpu",
	"PIP_DISABLE_PIP_VERSION_CHECK=1",
	"PIP_PROGRESS_BAR=off",
	"PIP_DEFAULT_TIMEOUT=60",
	"PYTHONUNBUFFERED=1",
}

// ImageExists reports whether a local Docker image is present.
func ImageExists(ctx context.Context, image string) bool {
	return exec.CommandContext(ctx, "docker", "image", "inspect", image).Run() == nil
}

// EnsureBase builds BaseImage if it is missing. The first build takes a few
// minutes; later runs reuse it.
func EnsureBase(ctx context.Context, logf func(string, ...any)) error {
	if ImageExists(ctx, BaseImage) {
		return nil
	}
	if logf != nil {
		logf("building base image %s (one-time, a few minutes)", BaseImage)
	}
	return BuildImage(ctx, BaseDockerfile, BaseImage)
}
