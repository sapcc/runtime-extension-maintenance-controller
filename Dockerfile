# SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company
#
# SPDX-License-Identifier: Apache-2.0

# Build the manager binary
FROM golang:1.24-alpine as builder

WORKDIR /workspace
ENV GOTOOLCHAIN=local
# Copy the Go Modules manifests
COPY go.mod go.mod
COPY go.sum go.sum
# cache deps before building and copying source so that we don't need to re-download as much
# and so that source changes don't invalidate our downloaded layer
RUN go mod download

COPY ./ /workspace/
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 GO111MODULE=on go build -ldflags="-s -w" -a -o runtime-extension-maintenance-controller main.go

# Use distroless as minimal base image to package the manager binary
# Refer to https://github.com/GoogleContainerTools/distroless for more details
FROM gcr.io/distroless/static:nonroot
WORKDIR /
LABEL source_repository="https://github.com/sapcc/runtime-extension-maintenance-controller"
COPY --from=builder /workspace/runtime-extension-maintenance-controller .
USER nonroot:nonroot

ENTRYPOINT ["/runtime-extension-maintenance-controller"]
