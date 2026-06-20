.PHONY: admin admin_dist build all clean

admin:
	cd admin && pnpm install --frozen-lockfile && pnpm build

admin_dist: admin
	rm -rf cmd/payserver/admin_dist
	mkdir -p cmd/payserver/admin_dist
	cp -r admin/dist/* cmd/payserver/admin_dist/

build: admin_dist
	go build -o payserver ./cmd/payserver

all: build

clean:
	rm -rf admin/node_modules admin/dist payserver
	git checkout -- cmd/payserver/admin_dist/ 2>/dev/null || true
