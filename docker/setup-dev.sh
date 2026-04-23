sudo docker run -d --name luckfox_ssh -p 8866:22 -v $(pwd):/home luckfoxtech/luckfox_pico:1.0 /sshd.sh

