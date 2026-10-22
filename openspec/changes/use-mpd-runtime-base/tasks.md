## 1. Base image

- [x] 1.1 Build `Dockerfile.base` with `GGML_NATIVE=OFF` on Alpine 3.21 with ca-certificates 20261020, push it to `sendrec-base` and pin its digest in `Dockerfile`; verify the deployed `whisper-cli` has no AVX-512 instructions.
- [x] 1.2 Add `scripts/check-ca-roots.sh` and run it in `Dockerfile.base`, `Dockerfile` and `Dockerfile.standalone`; verify it passes Alpine 3.21's current store and fails the 20260909 store, which still holds the three eMudhra roots.
- [x] 1.3 Build on Go 1.26.10; verify the deployed binary reports `go1.26.10`.

## 2. Builds without ECR access

- [x] 2.1 Build `Dockerfile.base` locally in CI, with a layer cache, and in `make docker-up` and `make e2e-up`; verify CI `docker-build` passes.
- [x] 2.2 Let the CDK image-publishing role pull `sendrec-base` through a repository policy; verify a pull of the pinned digest as that role succeeds.

## 3. Record

- [x] 3.1 Record the base digest, the deploy and the repository policy in `infrastructure/DEPLOYMENT.md` and the rebuild steps in `infrastructure/README.md`.
