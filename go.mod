module github.com/coopergillan/terraform-provider-redshift

go 1.13

require (
	github.com/hashicorp/terraform-plugin-sdk/v2 v2.4.4
	github.com/lib/pq v1.1.1
)

replace git.apache.org/thrift.git => github.com/apache/thrift v0.0.0-20180902110319-2566ecd5d999
