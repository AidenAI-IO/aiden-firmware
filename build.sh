docker run -u $(id -u):$(id -g) --rm -v $(pwd):/home -w /home luckfoxtech/luckfox_pico:1.0 /bin/bash -c "./_build.sh"

