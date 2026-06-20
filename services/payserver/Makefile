.PHONY: admin admin_dist build all clean

admin:
	cd admin && pnpm install --frozen-lockfile && pnpm build

admin_dist: admin
	# Preserve .gitkeep (tracked, needed so fresh checkouts have the dir
	# for //go:embed to find before any build runs) while replacing the
	# rest of the bundle output.
	find cmd/payserver/admin_dist -mindepth 1 ! -name .gitkeep -exec rm -rf {} +
	cp -r admin/dist/* cmd/payserver/admin_dist/

build: admin_dist
	go build -o payserver ./cmd/payserver

all: build

clean:
	rm -rf admin/node_modules admin/dist payserver
	git checkout -- cmd/payserver/admin_dist/ 2>/dev/null || true
