// Package testsupport holds helpers shared by tests in several packages.
// Nothing outside tests imports it.
package testsupport

// PTYType is a Python program that runs its arguments in a
// pseudo-terminal and types its standard input into it a line at a time,
// as a person would, then prints everything the terminal showed.
// Interactive shells only run their command hooks (bash-preexec's preexec,
// fish's fish_preexec) for commands typed into a terminal, so piping a
// script in would test nothing. Run it with: python3 -c PTYType argv...
//
// BT_TYPE_DELAY sets the seconds to wait after each line (default 0.25).
const PTYType = `
import os, pty, select, sys, time
delay = float(os.environ.get('BT_TYPE_DELAY', '0.25'))
lines = sys.stdin.read().splitlines()
pid, fd = pty.fork()
if pid == 0:
    os.execvp(sys.argv[1], sys.argv[1:])
out = b''
def drain(t):
    global out
    end = time.time() + t
    while time.time() < end:
        r, _, _ = select.select([fd], [], [], 0.05)
        if r:
            try:
                d = os.read(fd, 65536)
            except OSError:
                return False
            if not d:
                return False
            out += d
    return True
drain(1.0)
for line in lines:
    os.write(fd, line.encode() + b'\r')
    if not drain(delay):
        break
end = time.time() + 15
while time.time() < end and drain(0.2):
    pass
os.waitpid(pid, 0)
sys.stdout.buffer.write(out)
`
