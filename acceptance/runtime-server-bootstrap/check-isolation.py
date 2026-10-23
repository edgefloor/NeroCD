import json, sys
c = json.load(open(sys.argv[1]))[0]
h = c['HostConfig']
assert h['CgroupnsMode'] == 'private' and h['PidMode'] == ''
assert not h['Privileged'] and not h['Binds'] and not h['Devices']
assert len(c['Mounts']) == 1
m = c['Mounts'][0]
assert m['Type'] == 'volume' and m['Name'] == sys.argv[2] and m['Destination'] == '/var/lib/docker'
assert h['NetworkMode'] not in ('host', 'default', 'bridge')
assert not h['PortBindings']
print('pre-start isolation configuration verified; trusted fixture capabilities only')
