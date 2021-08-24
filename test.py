import json
from subprocess import PIPE, Popen


def popen_exec(cmd):
    pipe = Popen(cmd, shell=True, stdout=PIPE, stderr=PIPE)
    out = pipe.stdout.read().decode('utf-8')
    err = pipe.stderr.read().decode('utf-8')
    return out, err


cmd = './oplat -c abbott.cs.uchicago.edu -p 33301 -d abbott.cs.uchicago.edu -J -R'
out, err = popen_exec(cmd)
res = json.loads(out)

print(res["Protocol"])
print(int(res["UnloadedStats"]["MinRtt"])*1e-6)
